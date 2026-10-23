package dev.cardswitch.ledger;

import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;

import java.util.List;

/**
 * Double-entry ledger API. Every posting is a balanced set of entries
 * (sum of debits == sum of credits) written in one transaction.
 */
@RestController
@RequestMapping("/v1")
public class LedgerController {

    public enum Direction { DEBIT, CREDIT }
    public record Entry(String account, Direction direction, long amountMinor) {}
    public record PostingRequest(String idempotencyKey, String chargeId, String kind, String currency, List<Entry> entries) {}

    @PostMapping("/postings")
    public void post(@RequestBody PostingRequest req) {
        long debits = req.entries().stream().filter(e -> e.direction() == Direction.DEBIT).mapToLong(Entry::amountMinor).sum();
        long credits = req.entries().stream().filter(e -> e.direction() == Direction.CREDIT).mapToLong(Entry::amountMinor).sum();
        if (debits != credits) {
            throw new ResponseStatusException(HttpStatus.UNPROCESSABLE_ENTITY, "unbalanced posting");
        }
        // TODO: INSERT INTO transactions ... ON CONFLICT (idempotency_key) DO NOTHING,
        // then insert entries in the same tx.
        throw new ResponseStatusException(HttpStatus.NOT_IMPLEMENTED);
    }

    @GetMapping("/accounts/{id}/balance")
    public long balance(@PathVariable String id) {
        throw new ResponseStatusException(HttpStatus.NOT_IMPLEMENTED);
    }
}
