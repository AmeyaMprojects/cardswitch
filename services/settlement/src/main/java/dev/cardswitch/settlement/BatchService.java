package dev.cardswitch.settlement;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.sql.Timestamp;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

@Service
public class BatchService {

    public record Batch(UUID batchId, String merchantId, String currency, Instant windowStart, Instant windowEnd,
                        String status, long totalMinor) {}

    private final JdbcTemplate jdbc;
    private final Duration window;

    public BatchService(JdbcTemplate jdbc, @Value("${settlement.window-minutes}") long windowMinutes) {
        this.jdbc = jdbc;
        this.window = Duration.ofMinutes(windowMinutes);
    }

    /**
     * Adds an approved charge to the open batch for its merchant, currency and
     * window. If that batch already closed (a late event), the charge goes into
     * the batch for the current window instead.
     */
    @Transactional
    public void add(AuthEvent ev) {
        Instant at = ev.occurredAt() != null ? ev.occurredAt() : Instant.now();
        UUID batchId = openBatch(ev.merchantId(), ev.currency(), Windows.start(at, window))
                .or(() -> openBatch(ev.merchantId(), ev.currency(), Windows.start(Instant.now(), window)))
                .orElseThrow(() -> new IllegalStateException("no open batch for " + ev.merchantId()));
        jdbc.update("""
                INSERT INTO settlement_lines (batch_id, charge_id, amount_minor) VALUES (?, ?, ?)
                ON CONFLICT (charge_id) DO NOTHING""",
                batchId, UUID.fromString(ev.chargeId()), ev.amountMinor());
    }

    /** Removes a reversed charge from its batch if the batch is still open. */
    @Transactional
    public boolean remove(String chargeId) {
        int rows = jdbc.update("""
                DELETE FROM settlement_lines l USING settlement_batches b
                WHERE l.batch_id = b.batch_id AND b.status = 'OPEN' AND l.charge_id = ?""",
                UUID.fromString(chargeId));
        return rows > 0;
    }

    /** Returns the batch for this window if it is (or can be made) OPEN. */
    private Optional<UUID> openBatch(String merchantId, String currency, Instant start) {
        jdbc.update("""
                INSERT INTO settlement_batches (batch_id, merchant_id, currency, window_start, window_end, status)
                VALUES (?, ?, ?, ?, ?, 'OPEN')
                ON CONFLICT (merchant_id, currency, window_start) DO NOTHING""",
                UUID.randomUUID(), merchantId, currency, Timestamp.from(start), Timestamp.from(start.plus(window)));
        return jdbc.query("""
                SELECT batch_id FROM settlement_batches
                WHERE merchant_id = ? AND currency = ? AND window_start = ? AND status = 'OPEN'
                FOR SHARE""",
                (rs, n) -> rs.getObject(1, UUID.class), merchantId, currency, Timestamp.from(start)).stream().findFirst();
    }

    /** OPEN batches whose window ended before now (or all OPEN ones if force). */
    public List<UUID> dueForClose(boolean force) {
        String sql = "SELECT batch_id FROM settlement_batches WHERE status = 'OPEN'" + (force ? "" : " AND window_end <= now()");
        return jdbc.query(sql, (rs, n) -> rs.getObject(1, UUID.class));
    }

    /**
     * Marks the batch CLOSED and fixes its total. The row lock taken by the
     * UPDATE stops a concurrent add() from slipping a line in mid-close.
     * Returns the closed batch, or empty if someone else closed it first.
     */
    @Transactional
    public Optional<Batch> close(UUID batchId) {
        int rows = jdbc.update("""
                UPDATE settlement_batches b
                SET status = 'CLOSED', closed_at = now(),
                    total_minor = (SELECT COALESCE(SUM(amount_minor), 0) FROM settlement_lines WHERE batch_id = b.batch_id)
                WHERE batch_id = ? AND status = 'OPEN'""", batchId);
        return rows == 0 ? Optional.empty() : find(batchId);
    }

    public void recordSettlement(UUID batchId, UUID ledgerTxnId) {
        jdbc.update("UPDATE settlement_batches SET ledger_txn_id = ? WHERE batch_id = ?", ledgerTxnId, batchId);
    }

    public void recordReconciliation(UUID batchId, String status, String detailJson) {
        jdbc.update("UPDATE settlement_batches SET status = ?, reconciliation = ?::jsonb WHERE batch_id = ?",
                status, detailJson, batchId);
    }

    public Optional<Batch> find(UUID batchId) {
        return jdbc.query("SELECT * FROM settlement_batches WHERE batch_id = ?", (rs, n) -> new Batch(
                rs.getObject("batch_id", UUID.class), rs.getString("merchant_id"), rs.getString("currency"),
                rs.getTimestamp("window_start").toInstant(), rs.getTimestamp("window_end").toInstant(),
                rs.getString("status"), rs.getLong("total_minor")), batchId).stream().findFirst();
    }

    public List<Batch> list(String merchantId) {
        return jdbc.query("SELECT batch_id FROM settlement_batches WHERE merchant_id = ? ORDER BY window_start DESC LIMIT 50",
                (rs, n) -> rs.getObject(1, UUID.class), merchantId).stream()
                .map(id -> find(id).orElseThrow()).toList();
    }
}
