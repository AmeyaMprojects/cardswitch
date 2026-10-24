package dev.cardswitch.vault;

import java.util.Optional;

public interface TokenRepository {
    Optional<TokenRecord> findByFingerprint(byte[] panHmac);

    Optional<TokenRecord> findByToken(String token);

    /** Inserts unless the fingerprint already exists. Returns true if inserted. */
    boolean insertIfAbsent(TokenRecord record);
}
