package dev.cardswitch.settlement;

import org.springframework.kafka.annotation.KafkaListener;
import org.springframework.stereotype.Component;

/**
 * Consumes captured authorizations and groups them into settlement batches
 * per (merchant, currency, cutoff window).
 */
@Component
public class CaptureListener {

    @KafkaListener(topics = "auth.captured")
    public void onCapture(String event) {
        // TODO: parse event, append to open batch for merchant/currency.
    }

    @KafkaListener(topics = "auth.reversed")
    public void onReversal(String event) {
        // TODO: remove from open batch or emit adjustment if batch already closed.
    }
}
