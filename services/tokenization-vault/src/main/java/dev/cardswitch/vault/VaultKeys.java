package dev.cardswitch.vault;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

import javax.crypto.Mac;
import javax.crypto.SecretKey;
import javax.crypto.spec.SecretKeySpec;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.util.Base64;

/**
 * Holds the vault's two keys. They are separate on purpose: the HMAC key only
 * fingerprints PANs for lookup, so leaking it does not let anyone decrypt.
 */
@Component
public class VaultKeys {
    private final SecretKey encryptionKey;
    private final SecretKeySpec hmacKey;

    public VaultKeys(@Value("${vault.master-key}") String masterKeyB64,
                     @Value("${vault.hmac-key}") String hmacKeyB64) {
        this.encryptionKey = new SecretKeySpec(decode256("vault.master-key", masterKeyB64), "AES");
        this.hmacKey = new SecretKeySpec(decode256("vault.hmac-key", hmacKeyB64), "HmacSHA256");
    }

    public SecretKey encryptionKey() {
        return encryptionKey;
    }

    /** HMAC-SHA256 of the PAN: the same card always gives the same fingerprint. */
    public byte[] fingerprint(String pan) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(hmacKey);
            return mac.doFinal(pan.getBytes(StandardCharsets.UTF_8));
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException(e);
        }
    }

    private static byte[] decode256(String name, String b64) {
        if (b64 == null || b64.isBlank()) {
            throw new IllegalStateException(name + " is not set");
        }
        byte[] key = Base64.getDecoder().decode(b64.trim());
        if (key.length != 32) {
            throw new IllegalStateException(name + " must be 32 bytes, got " + key.length);
        }
        return key;
    }
}
