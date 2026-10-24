package dev.cardswitch.vault;

import org.springframework.stereotype.Service;

import java.security.GeneralSecurityException;
import java.security.SecureRandom;
import java.util.Optional;

@Service
public class TokenService {
    public record TokenInfo(String token, String bin, String last4) {}

    public static class InvalidCardException extends RuntimeException {
        public InvalidCardException() {
            super("card number fails validation");
        }
    }

    private static final String ALPHABET = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
    private static final int KEY_VERSION = 1;

    private final TokenRepository repo;
    private final VaultKeys keys;
    private final PanCipher cipher;
    private final SecureRandom random = new SecureRandom();

    public TokenService(TokenRepository repo, VaultKeys keys) {
        this.repo = repo;
        this.keys = keys;
        this.cipher = new PanCipher(keys.encryptionKey());
    }

    /**
     * Returns the token for pan, creating one the first time this card is seen.
     * The same card always gets the same token, found by its HMAC fingerprint.
     */
    public TokenInfo tokenize(String pan) {
        if (!Luhn.isValid(pan)) {
            throw new InvalidCardException();
        }
        byte[] fingerprint = keys.fingerprint(pan);
        Optional<TokenRecord> existing = repo.findByFingerprint(fingerprint);
        if (existing.isPresent()) {
            return info(existing.get());
        }

        String token = newToken();
        byte[] encrypted;
        try {
            encrypted = cipher.encrypt(pan, token);
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("encryption failed", e);
        }
        TokenRecord record = new TokenRecord(token, fingerprint, encrypted,
                pan.substring(0, 6), pan.substring(pan.length() - 4), KEY_VERSION);
        if (repo.insertIfAbsent(record)) {
            return info(record);
        }
        // Another request tokenized the same card between our lookup and insert.
        return info(repo.findByFingerprint(fingerprint).orElseThrow());
    }

    /** Token metadata, never the PAN. */
    public Optional<TokenInfo> lookup(String token) {
        return repo.findByToken(token).map(TokenService::info);
    }

    public Optional<String> detokenize(String token) {
        return repo.findByToken(token).map(r -> {
            try {
                return cipher.decrypt(r.panCipher(), r.token());
            } catch (GeneralSecurityException e) {
                throw new IllegalStateException("stored ciphertext failed authentication for " + token, e);
            }
        });
    }

    private String newToken() {
        StringBuilder sb = new StringBuilder("tok_");
        for (int i = 0; i < 24; i++) {
            sb.append(ALPHABET.charAt(random.nextInt(ALPHABET.length())));
        }
        return sb.toString();
    }

    private static TokenInfo info(TokenRecord r) {
        return new TokenInfo(r.token(), r.bin(), r.last4());
    }
}
