package dev.cardswitch.settlement;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.kafka.annotation.KafkaListener;
import org.springframework.stereotype.Component;

/**
 * Consumes authorization events. Both handlers are idempotent, so a
 * redelivered message (Kafka is at-least-once) changes nothing.
 */
@Component
public class AuthEventListener {
    private static final Logger log = LoggerFactory.getLogger(AuthEventListener.class);

    private final BatchService batches;
    private final ObjectMapper json;

    public AuthEventListener(BatchService batches, ObjectMapper json) {
        this.batches = batches;
        this.json = json;
    }

    @KafkaListener(topics = "auth.approved")
    public void onApproved(String message) throws JsonProcessingException {
        batches.add(json.readValue(message, AuthEvent.class));
    }

    @KafkaListener(topics = "auth.reversed")
    public void onReversed(String message) throws JsonProcessingException {
        AuthEvent ev = json.readValue(message, AuthEvent.class);
        if (!batches.remove(ev.chargeId())) {
            log.info("reversal for {} ({}): no open settlement line to remove", ev.chargeId(), ev.reason());
        }
    }
}
