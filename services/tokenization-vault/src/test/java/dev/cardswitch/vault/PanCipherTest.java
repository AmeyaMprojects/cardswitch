package dev.cardswitch.vault;

import org.junit.jupiter.api.Test;

import javax.crypto.AEADBadTagException;
import javax.crypto.spec.SecretKeySpec;

import static org.junit.jupiter.api.Assertions.*;

class PanCipherTest {
    private final PanCipher cipher = new PanCipher(new SecretKeySpec(new byte[32], "AES"));

    @Test
    void roundTrips() throws Exception {
        byte[] blob = cipher.encrypt("4111111111111111", "tok_a");
        assertEquals("4111111111111111", cipher.decrypt(blob, "tok_a"));
    }

    @Test
    void freshIvEachTime() throws Exception {
        assertFalse(java.util.Arrays.equals(
                cipher.encrypt("4111111111111111", "tok_a"),
                cipher.encrypt("4111111111111111", "tok_a")));
    }

    @Test
    void ciphertextIsBoundToItsToken() throws Exception {
        byte[] blob = cipher.encrypt("4111111111111111", "tok_a");
        assertThrows(AEADBadTagException.class, () -> cipher.decrypt(blob, "tok_b"));
    }

    @Test
    void tamperingIsDetected() throws Exception {
        byte[] blob = cipher.encrypt("4111111111111111", "tok_a");
        blob[blob.length - 1] ^= 1;
        assertThrows(AEADBadTagException.class, () -> cipher.decrypt(blob, "tok_a"));
    }
}
