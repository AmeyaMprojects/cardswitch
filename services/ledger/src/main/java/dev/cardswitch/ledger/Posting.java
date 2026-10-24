package dev.cardswitch.ledger;

import java.util.List;
import java.util.Set;

/** A balanced set of entries recorded as one ledger transaction. */
public record Posting(String idempotencyKey, String chargeId, String kind, String currency, List<Entry> entries) {

    public enum Direction { DEBIT, CREDIT }

    public record Entry(String account, Direction direction, long amountMinor) {}

    public static final Set<String> KINDS = Set.of("AUTH_HOLD", "CAPTURE", "REVERSAL", "SETTLEMENT");

    /** Returns null if the posting is valid, otherwise why it is not. */
    public String validate() {
        if (idempotencyKey == null || idempotencyKey.isBlank()) {
            return "idempotency_key is required";
        }
        if (kind == null || !KINDS.contains(kind)) {
            return "kind must be one of " + KINDS;
        }
        if (currency == null || !currency.matches("[A-Z]{3}")) {
            return "currency must be a 3-letter ISO code";
        }
        if (entries == null || entries.size() < 2) {
            return "a posting needs at least two entries";
        }
        long debits = 0, credits = 0;
        for (Entry e : entries) {
            if (e.account() == null || e.account().isBlank() || e.direction() == null) {
                return "every entry needs an account and a direction";
            }
            if (e.amountMinor() <= 0) {
                return "entry amounts must be positive";
            }
            try {
                if (e.direction() == Direction.DEBIT) {
                    debits = Math.addExact(debits, e.amountMinor());
                } else {
                    credits = Math.addExact(credits, e.amountMinor());
                }
            } catch (ArithmeticException overflow) {
                return "entry amounts overflow";
            }
        }
        if (debits != credits) {
            return "unbalanced posting: debits " + debits + " != credits " + credits;
        }
        return null;
    }

    /**
     * Account type from its name: "...:payable" accounts are liabilities
     * (money we owe), everything else is an asset (money owed to us or held).
     */
    public static String accountKind(String accountId) {
        return accountId.endsWith(":payable") ? "LIABILITY" : "ASSET";
    }
}
