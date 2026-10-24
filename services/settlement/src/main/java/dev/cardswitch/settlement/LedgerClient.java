package dev.cardswitch.settlement;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;
import org.springframework.web.client.RestClient;

import java.util.List;
import java.util.Map;
import java.util.UUID;

@Component
public class LedgerClient {
    private final RestClient http;

    public LedgerClient(@Value("${settlement.ledger-url}") String ledgerUrl) {
        this.http = RestClient.builder().baseUrl(ledgerUrl).build();
    }

    record PostResult(UUID txnId, boolean duplicate) {}

    /**
     * Moves the batch total from the merchant's receivable to cash. Keyed by
     * batch ID, so re-running a close never pays a merchant twice.
     */
    public UUID postSettlement(BatchService.Batch b) {
        String receivable = "merchant:" + b.merchantId() + ":receivable";
        String cash = "merchant:" + b.merchantId() + ":cash";
        Map<String, Object> posting = Map.of(
                "idempotency_key", "settle:" + b.batchId(),
                "kind", "SETTLEMENT",
                "currency", b.currency(),
                "entries", List.of(
                        Map.of("account", cash, "direction", "DEBIT", "amount_minor", b.totalMinor()),
                        Map.of("account", receivable, "direction", "CREDIT", "amount_minor", b.totalMinor())));
        PostResult result = http.post().uri("/v1/postings").body(posting).retrieve().body(PostResult.class);
        return result == null ? null : result.txnId();
    }
}
