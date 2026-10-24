package dev.cardswitch.vault;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.core.RowMapper;
import org.springframework.stereotype.Repository;

import java.util.Optional;

@Repository
public class JdbcTokenRepository implements TokenRepository {
    private static final RowMapper<TokenRecord> ROW = (rs, n) -> new TokenRecord(
            rs.getString("token"), rs.getBytes("pan_hmac"), rs.getBytes("pan_cipher"),
            rs.getString("bin"), rs.getString("last4"), rs.getInt("key_version"));

    private final JdbcTemplate jdbc;

    public JdbcTokenRepository(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    @Override
    public Optional<TokenRecord> findByFingerprint(byte[] panHmac) {
        return jdbc.query("SELECT * FROM vault_tokens WHERE pan_hmac = ?", ROW, panHmac).stream().findFirst();
    }

    @Override
    public Optional<TokenRecord> findByToken(String token) {
        return jdbc.query("SELECT * FROM vault_tokens WHERE token = ?", ROW, token).stream().findFirst();
    }

    @Override
    public boolean insertIfAbsent(TokenRecord r) {
        int rows = jdbc.update("""
                INSERT INTO vault_tokens (token, pan_hmac, pan_cipher, bin, last4, key_version)
                VALUES (?, ?, ?, ?, ?, ?)
                ON CONFLICT (pan_hmac) DO NOTHING""",
                r.token(), r.panHmac(), r.panCipher(), r.bin(), r.last4(), r.keyVersion());
        return rows == 1;
    }
}
