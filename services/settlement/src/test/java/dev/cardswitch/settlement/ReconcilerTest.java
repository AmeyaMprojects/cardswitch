package dev.cardswitch.settlement;

import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.*;

class ReconcilerTest {
    private final UUID batch = UUID.randomUUID();

    @Test
    void matchingBatchIsClean() {
        var r = Reconciler.compare(batch, Map.of("c1", 1999L, "c2", 500L), Map.of("c1", 1999L, "c2", 500L));
        assertTrue(r.clean());
        assertEquals(2499, r.batchTotalMinor());
        assertEquals(2499, r.ledgerTotalMinor());
    }

    @Test
    void lineWithoutHold() {
        var r = Reconciler.compare(batch, Map.of("c1", 1999L), Map.of());
        assertFalse(r.clean());
        assertEquals(Reconciler.MISSING_IN_LEDGER, r.mismatches().get(0).type());
    }

    @Test
    void amountDrift() {
        var r = Reconciler.compare(batch, Map.of("c1", 1999L), Map.of("c1", 1900L));
        assertEquals(Reconciler.AMOUNT_DRIFT, r.mismatches().get(0).type());
        assertEquals(1900, r.mismatches().get(0).ledgerMinor());
    }

    @Test
    void heldChargeMissingFromBatch() {
        var r = Reconciler.compare(batch, Map.of("c1", 1999L), Map.of("c1", 1999L, "c9", 700L));
        assertEquals(1, r.mismatches().size());
        assertEquals("c9", r.mismatches().get(0).chargeId());
        assertEquals(Reconciler.MISSING_IN_BATCH, r.mismatches().get(0).type());
        assertEquals(1999, r.ledgerTotalMinor(), "only holds for the batch's own lines count toward its ledger total");
    }

    @Test
    void emptyBatchIsClean() {
        assertTrue(Reconciler.compare(batch, Map.of(), Map.of()).clean());
    }
}
