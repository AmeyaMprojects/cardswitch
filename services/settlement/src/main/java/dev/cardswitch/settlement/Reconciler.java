package dev.cardswitch.settlement;

import org.springframework.stereotype.Service;

/**
 * Compares a closed settlement batch against ledger totals and flags mismatches
 * (missing captures, orphan reversals, amount drift).
 */
@Service
public class Reconciler {

    public record Result(String batchId, long batchTotalMinor, long ledgerTotalMinor, int mismatches) {
        public boolean clean() { return mismatches == 0 && batchTotalMinor == ledgerTotalMinor; }
    }

    public Result reconcile(String batchId) {
        // TODO: SUM batch lines vs SUM ledger entries for batch's charge_ids.
        throw new UnsupportedOperationException("not implemented");
    }
}
