package dev.cardswitch.vault;

/** One row of vault_tokens. */
public record TokenRecord(String token, byte[] panHmac, byte[] panCipher, String bin, String last4, int keyVersion) {}
