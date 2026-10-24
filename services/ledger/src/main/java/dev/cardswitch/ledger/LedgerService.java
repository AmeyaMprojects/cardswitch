package dev.cardswitch.ledger;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.util.List;
import java.util.Optional;
import java.util.UUID;

@Service
public class LedgerService {

    public record PostResult(UUID txnId, boolean duplicate) {}

    public record Balance(String accountId, String kind, String currency, long balanceMinor) {}

    public static class RejectedException extends RuntimeException {
        public RejectedException(String message) {
            super(message);
        }
    }

    private final JdbcTemplate jdbc;

    public LedgerService(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    /**
     * Records a posting exactly once per idempotency key. The transaction row
     * and its entries commit together; the database's deferred trigger rejects
     * the commit if they don't balance, even if this code had a bug.
     */
    @Transactional
    public PostResult post(Posting p) {
        String invalid = p.validate();
        if (invalid != null) {
            throw new RejectedException(invalid);
        }

        UUID txnId = UUID.randomUUID();
        UUID chargeId = p.chargeId() == null || p.chargeId().isBlank() ? null : UUID.fromString(p.chargeId());
        int inserted = jdbc.update("""
                INSERT INTO ledger_transactions (txn_id, idempotency_key, charge_id, kind)
                VALUES (?, ?, ?, ?)
                ON CONFLICT (idempotency_key) DO NOTHING""",
                txnId, p.idempotencyKey(), chargeId, p.kind());
        if (inserted == 0) {
            UUID existing = jdbc.queryForObject(
                    "SELECT txn_id FROM ledger_transactions WHERE idempotency_key = ?", UUID.class, p.idempotencyKey());
            return new PostResult(existing, true);
        }

        for (Posting.Entry e : p.entries()) {
            ensureAccount(e.account(), p.currency());
        }
        jdbc.batchUpdate(
                "INSERT INTO ledger_entries (txn_id, account_id, direction, amount_minor) VALUES (?, ?, ?, ?)",
                p.entries().stream()
                        .map(e -> new Object[]{txnId, e.account(), e.direction().name(), e.amountMinor()})
                        .toList());
        return new PostResult(txnId, false);
    }

    private void ensureAccount(String accountId, String currency) {
        jdbc.update("INSERT INTO accounts (account_id, kind, currency) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
                accountId, Posting.accountKind(accountId), currency);
        String actual = jdbc.queryForObject("SELECT currency FROM accounts WHERE account_id = ?", String.class, accountId);
        if (!currency.equals(actual)) {
            throw new RejectedException("account " + accountId + " is in " + actual + ", posting is in " + currency);
        }
    }

    /**
     * Balance in the account's natural direction: debits minus credits for
     * assets, credits minus debits for liabilities. Computed from entries, so
     * it can never drift from the history.
     */
    public Optional<Balance> balance(String accountId) {
        List<Balance> rows = jdbc.query("""
                SELECT a.account_id, a.kind, a.currency,
                       COALESCE(SUM(CASE e.direction WHEN 'DEBIT' THEN e.amount_minor ELSE -e.amount_minor END), 0) AS net
                FROM accounts a LEFT JOIN ledger_entries e ON e.account_id = a.account_id
                WHERE a.account_id = ?
                GROUP BY a.account_id, a.kind, a.currency""",
                (rs, n) -> {
                    String kind = rs.getString("kind");
                    long net = rs.getLong("net");
                    boolean creditNormal = kind.equals("LIABILITY") || kind.equals("REVENUE");
                    return new Balance(rs.getString("account_id"), kind, rs.getString("currency"), creditNormal ? -net : net);
                },
                accountId);
        return rows.stream().findFirst();
    }
}
