package dev.cardswitch.ledger;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;

@RestController
@RequestMapping("/v1")
public class LedgerController {

    private final LedgerService ledger;

    public LedgerController(LedgerService ledger) {
        this.ledger = ledger;
    }

    /** 201 for a new posting, 200 when the idempotency key was already used. */
    @PostMapping("/postings")
    public ResponseEntity<LedgerService.PostResult> post(@RequestBody Posting posting) {
        try {
            LedgerService.PostResult result = ledger.post(posting);
            return ResponseEntity.status(result.duplicate() ? HttpStatus.OK : HttpStatus.CREATED).body(result);
        } catch (LedgerService.RejectedException | IllegalArgumentException e) {
            throw new ResponseStatusException(HttpStatus.UNPROCESSABLE_ENTITY, e.getMessage());
        }
    }

    @GetMapping("/accounts/{id}/balance")
    public LedgerService.Balance balance(@PathVariable String id) {
        return ledger.balance(id).orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));
    }
}
