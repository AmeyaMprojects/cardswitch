package dev.cardswitch.vault;

import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;

@RestController
@RequestMapping("/v1/tokens")
public class TokenController {

    public record TokenizeRequest(String pan) {}
    public record TokenizeResponse(String token, String bin, String last4) {}

    @PostMapping
    public TokenizeResponse tokenize(@RequestBody TokenizeRequest req) {
        // TODO: Luhn check, deterministic lookup by HMAC(pan) to reuse tokens,
        // generate random token, store PanCipher.encrypt(pan, token).
        throw new ResponseStatusException(HttpStatus.NOT_IMPLEMENTED);
    }

    @PostMapping("/{token}/detokenize")
    public String detokenize(@PathVariable String token) {
        // TODO: restrict to issuer-facing callers only (mTLS / service allowlist).
        throw new ResponseStatusException(HttpStatus.NOT_IMPLEMENTED);
    }
}
