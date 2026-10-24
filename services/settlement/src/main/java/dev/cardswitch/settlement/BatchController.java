package dev.cardswitch.settlement;

import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;

import java.util.List;
import java.util.UUID;

@RestController
@RequestMapping("/v1/batches")
public class BatchController {
    private final BatchService batches;
    private final SettlementJob job;
    private final Reconciler reconciler;

    public BatchController(BatchService batches, SettlementJob job, Reconciler reconciler) {
        this.batches = batches;
        this.job = job;
        this.reconciler = reconciler;
    }

    @GetMapping
    public List<BatchService.Batch> list(@RequestParam("merchant_id") String merchantId) {
        return batches.list(merchantId);
    }

    @GetMapping("/{id}")
    public BatchService.Batch get(@PathVariable UUID id) {
        return batches.find(id).orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));
    }

    @GetMapping("/{id}/reconciliation")
    public Reconciler.Result reconciliation(@PathVariable UUID id) {
        batches.find(id).orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));
        return reconciler.reconcile(id);
    }

    /** Closes batches now instead of waiting for the window to end. */
    @PostMapping("/close")
    public List<Reconciler.Result> close(@RequestParam(value = "force", defaultValue = "false") boolean force) {
        return job.run(force);
    }
}
