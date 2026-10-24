package dev.cardswitch.ledger;

import org.junit.jupiter.api.Test;

import java.util.List;

import static dev.cardswitch.ledger.Posting.Direction.CREDIT;
import static dev.cardswitch.ledger.Posting.Direction.DEBIT;
import static org.junit.jupiter.api.Assertions.*;

class PostingTest {

    private static Posting posting(Posting.Entry... entries) {
        return new Posting("hold:c1", null, "AUTH_HOLD", "USD", List.of(entries));
    }

    @Test
    void balancedPostingIsValid() {
        assertNull(posting(
                new Posting.Entry("merchant:m1:receivable", DEBIT, 1999),
                new Posting.Entry("issuer:a:payable", CREDIT, 1999)).validate());
    }

    @Test
    void splitEntriesMayBalance() {
        assertNull(posting(
                new Posting.Entry("merchant:m1:receivable", DEBIT, 2000),
                new Posting.Entry("issuer:a:payable", CREDIT, 1940),
                new Posting.Entry("network:fees", CREDIT, 60)).validate());
    }

    @Test
    void unbalancedIsRejected() {
        assertTrue(posting(
                new Posting.Entry("merchant:m1:receivable", DEBIT, 1999),
                new Posting.Entry("issuer:a:payable", CREDIT, 1998)).validate().startsWith("unbalanced"));
    }

    @Test
    void singleEntryIsRejected() {
        assertNotNull(posting(new Posting.Entry("merchant:m1:receivable", DEBIT, 1999)).validate());
    }

    @Test
    void nonPositiveAmountsAreRejected() {
        assertNotNull(posting(
                new Posting.Entry("a", DEBIT, 0),
                new Posting.Entry("b", CREDIT, 0)).validate());
        assertNotNull(posting(
                new Posting.Entry("a", DEBIT, -5),
                new Posting.Entry("b", CREDIT, -5)).validate());
    }

    @Test
    void overflowIsRejected() {
        assertNotNull(posting(
                new Posting.Entry("a", DEBIT, Long.MAX_VALUE),
                new Posting.Entry("a", DEBIT, 1),
                new Posting.Entry("b", CREDIT, 1)).validate());
    }

    @Test
    void headerFieldsAreChecked() {
        List<Posting.Entry> ok = List.of(new Posting.Entry("a", DEBIT, 1), new Posting.Entry("b", CREDIT, 1));
        assertNotNull(new Posting("", null, "AUTH_HOLD", "USD", ok).validate());
        assertNotNull(new Posting("k", null, "REFUND", "USD", ok).validate());
        assertNotNull(new Posting("k", null, "AUTH_HOLD", "usd", ok).validate());
    }

    @Test
    void accountKindFromName() {
        assertEquals("LIABILITY", Posting.accountKind("issuer:issuer-a:payable"));
        assertEquals("ASSET", Posting.accountKind("merchant:m1:receivable"));
        assertEquals("ASSET", Posting.accountKind("merchant:m1:cash"));
    }
}
