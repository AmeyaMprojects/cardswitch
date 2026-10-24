package dev.cardswitch.settlement;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;

import java.util.*;

/**
 * Compares a closed settlement batch against the ledger. Every line must
 * match an AUTH_HOLD for the same charge and amount, and every held charge
 * from the merchant in the batch window must be in some batch.
 */
@Service
public class Reconciler {

    public record Mismatch(String chargeId, String type, long batchMinor, long ledgerMinor) {}

    public record Result(UUID batchId, long batchTotalMinor, long ledgerTotalMinor, List<Mismatch> mismatches) {
        public boolean clean() {
            return mismatches.isEmpty() && batchTotalMinor == ledgerTotalMinor;
        }
    }

    public static final String MISSING_IN_LEDGER = "MISSING_IN_LEDGER";
    public static final String AMOUNT_DRIFT = "AMOUNT_DRIFT";
    public static final String MISSING_IN_BATCH = "MISSING_IN_BATCH";

    private final JdbcTemplate jdbc;

    public Reconciler(JdbcTemplate jdbc) {
        this.jdbc = jdbc;
    }

    public Result reconcile(UUID batchId) {
        Map<String, Long> lines = new HashMap<>();
        jdbc.query("SELECT charge_id, amount_minor FROM settlement_lines WHERE batch_id = ?",
                rs -> { lines.put(rs.getString(1), rs.getLong(2)); }, batchId);

        // Holds for the batch's own charges, plus held charges from the same
        // merchant, currency and window that never reached any batch.
        Map<String, Long> held = new HashMap<>();
        jdbc.query("""
                SELECT t.charge_id, SUM(e.amount_minor)
                FROM ledger_transactions t
                JOIN ledger_entries e ON e.txn_id = t.txn_id AND e.direction = 'DEBIT'
                JOIN charges c ON c.charge_id = t.charge_id
                JOIN settlement_batches b ON b.batch_id = ?
                WHERE t.kind = 'AUTH_HOLD'
                  AND (t.charge_id IN (SELECT charge_id FROM settlement_lines WHERE batch_id = b.batch_id)
                       OR (c.merchant_id = b.merchant_id AND c.currency = b.currency
                           AND c.created_at >= b.window_start AND c.created_at < b.window_end
                           AND NOT EXISTS (SELECT 1 FROM settlement_lines l WHERE l.charge_id = t.charge_id)))
                GROUP BY t.charge_id""",
                rs -> { held.put(rs.getString(1), rs.getLong(2)); }, batchId);

        return compare(batchId, lines, held);
    }

    /** Pure comparison, separated from the queries so it can be unit tested. */
    static Result compare(UUID batchId, Map<String, Long> lines, Map<String, Long> held) {
        List<Mismatch> mismatches = new ArrayList<>();
        for (var line : lines.entrySet()) {
            Long ledger = held.get(line.getKey());
            if (ledger == null) {
                mismatches.add(new Mismatch(line.getKey(), MISSING_IN_LEDGER, line.getValue(), 0));
            } else if (!ledger.equals(line.getValue())) {
                mismatches.add(new Mismatch(line.getKey(), AMOUNT_DRIFT, line.getValue(), ledger));
            }
        }
        for (var hold : held.entrySet()) {
            if (!lines.containsKey(hold.getKey())) {
                mismatches.add(new Mismatch(hold.getKey(), MISSING_IN_BATCH, 0, hold.getValue()));
            }
        }
        mismatches.sort(Comparator.comparing(Mismatch::chargeId));
        long batchTotal = lines.values().stream().mapToLong(Long::longValue).sum();
        long ledgerTotal = held.entrySet().stream()
                .filter(e -> lines.containsKey(e.getKey()))
                .mapToLong(Map.Entry::getValue).sum();
        return new Result(batchId, batchTotal, ledgerTotal, mismatches);
    }
}
