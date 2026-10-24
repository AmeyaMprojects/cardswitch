package dev.cardswitch.settlement;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

/** Closes due batches, posts them to the ledger and reconciles them. */
@Component
public class SettlementJob {
    private static final Logger log = LoggerFactory.getLogger(SettlementJob.class);

    private final BatchService batches;
    private final LedgerClient ledger;
    private final Reconciler reconciler;
    private final ObjectMapper json;

    public SettlementJob(BatchService batches, LedgerClient ledger, Reconciler reconciler, ObjectMapper json) {
        this.batches = batches;
        this.ledger = ledger;
        this.reconciler = reconciler;
        this.json = json;
    }

    @Scheduled(fixedDelayString = "${settlement.close-check-ms}")
    public void closeDue() {
        run(false);
    }

    /** Closes due batches (or all open ones if force) and returns their reconciliation. */
    public List<Reconciler.Result> run(boolean force) {
        List<Reconciler.Result> results = new ArrayList<>();
        for (UUID id : batches.dueForClose(force)) {
            batches.close(id).ifPresent(batch -> results.add(settle(batch)));
        }
        return results;
    }

    private Reconciler.Result settle(BatchService.Batch batch) {
        if (batch.totalMinor() > 0) {
            try {
                batches.recordSettlement(batch.batchId(), ledger.postSettlement(batch));
            } catch (RuntimeException e) {
                log.error("batch {}: ledger settlement posting failed, will show as unsettled", batch.batchId(), e);
            }
        }
        Reconciler.Result result = reconciler.reconcile(batch.batchId());
        String status = result.clean() ? "RECONCILED" : "MISMATCH";
        try {
            batches.recordReconciliation(batch.batchId(), status, json.writeValueAsString(result));
        } catch (JsonProcessingException e) {
            throw new IllegalStateException(e);
        }
        log.info("batch {} merchant={} total={} {} mismatches={}",
                batch.batchId(), batch.merchantId(), batch.totalMinor(), status, result.mismatches().size());
        return result;
    }
}
