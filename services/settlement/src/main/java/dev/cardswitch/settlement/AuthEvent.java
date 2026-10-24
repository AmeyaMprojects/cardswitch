package dev.cardswitch.settlement;

import java.time.Instant;

/** An auth.approved or auth.reversed event, as published by the switch. */
public record AuthEvent(String chargeId, String merchantId, String issuerId, long amountMinor,
                        String currency, String reason, Instant occurredAt) {}
