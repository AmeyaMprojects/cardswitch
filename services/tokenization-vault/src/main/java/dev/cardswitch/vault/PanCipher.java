package dev.cardswitch.vault;

import javax.crypto.Cipher;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.SecureRandom;

/**
 * AES-256-GCM encryption for PANs at rest. Output layout: 12-byte IV || ciphertext+tag.
 * The token is bound as AAD so a ciphertext cannot be swapped onto another token.
 */
public final class PanCipher {
    private static final int IV_LEN = 12;
    private static final int TAG_BITS = 128;
    private final SecretKey key;
    private final SecureRandom random = new SecureRandom();

    public PanCipher(SecretKey key) {
        this.key = key;
    }

    public byte[] encrypt(String pan, String token) throws GeneralSecurityException {
        byte[] iv = new byte[IV_LEN];
        random.nextBytes(iv);
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.ENCRYPT_MODE, key, new GCMParameterSpec(TAG_BITS, iv));
        c.updateAAD(token.getBytes(StandardCharsets.UTF_8));
        byte[] ct = c.doFinal(pan.getBytes(StandardCharsets.UTF_8));
        return ByteBuffer.allocate(IV_LEN + ct.length).put(iv).put(ct).array();
    }

    public String decrypt(byte[] blob, String token) throws GeneralSecurityException {
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(TAG_BITS, blob, 0, IV_LEN));
        c.updateAAD(token.getBytes(StandardCharsets.UTF_8));
        return new String(c.doFinal(blob, IV_LEN, blob.length - IV_LEN), StandardCharsets.UTF_8);
    }
}
