package dev.cardswitch.vault;

import org.junit.jupiter.api.Test;

import java.util.*;

import static org.junit.jupiter.api.Assertions.*;

class TokenServiceTest {

    static class MemoryRepo implements TokenRepository {
        final Map<String, TokenRecord> byToken = new HashMap<>();
        boolean raceOnInsert;
        TokenRecord racer;

        @Override
        public Optional<TokenRecord> findByFingerprint(byte[] panHmac) {
            return byToken.values().stream().filter(r -> Arrays.equals(r.panHmac(), panHmac)).findFirst();
        }

        @Override
        public Optional<TokenRecord> findByToken(String token) {
            return Optional.ofNullable(byToken.get(token));
        }

        @Override
        public boolean insertIfAbsent(TokenRecord r) {
            if (raceOnInsert) {
                racer = new TokenRecord("tok_racer", r.panHmac(), r.panCipher(), r.bin(), r.last4(), 1);
                byToken.put(racer.token(), racer);
                return false;
            }
            if (findByFingerprint(r.panHmac()).isPresent()) {
                return false;
            }
            byToken.put(r.token(), r);
            return true;
        }
    }

    private static final String KEY = Base64.getEncoder().encodeToString(new byte[32]);
    private static final String HMAC = Base64.getEncoder().encodeToString("0123456789abcdef0123456789abcdef".getBytes());

    private final MemoryRepo repo = new MemoryRepo();
    private final TokenService service = new TokenService(repo, new VaultKeys(KEY, HMAC));

    @Test
    void tokenizeReturnsMetadataNotPan() {
        TokenService.TokenInfo info = service.tokenize("4111111111111111");
        assertTrue(info.token().startsWith("tok_"));
        assertEquals("411111", info.bin());
        assertEquals("1111", info.last4());
        TokenRecord stored = repo.byToken.get(info.token());
        assertFalse(new String(stored.panCipher()).contains("4111111111111111"), "PAN must be stored encrypted");
    }

    @Test
    void sameCardSameToken() {
        assertEquals(service.tokenize("4111111111111111").token(), service.tokenize("4111111111111111").token());
        assertNotEquals(service.tokenize("4111111111111111").token(), service.tokenize("5555555555554444").token());
    }

    @Test
    void concurrentTokenizeReturnsTheWinner() {
        repo.raceOnInsert = true;
        assertEquals("tok_racer", service.tokenize("4111111111111111").token());
    }

    @Test
    void rejectsInvalidCard() {
        assertThrows(TokenService.InvalidCardException.class, () -> service.tokenize("4111111111111112"));
    }

    @Test
    void detokenizeRoundTrips() {
        String token = service.tokenize("5555555555554444").token();
        assertEquals(Optional.of("5555555555554444"), service.detokenize(token));
        assertEquals(Optional.empty(), service.detokenize("tok_missing"));
    }

    @Test
    void rejectsShortKeys() {
        assertThrows(IllegalStateException.class, () -> new VaultKeys(Base64.getEncoder().encodeToString(new byte[16]), HMAC));
        assertThrows(IllegalStateException.class, () -> new VaultKeys("", HMAC));
    }
}
