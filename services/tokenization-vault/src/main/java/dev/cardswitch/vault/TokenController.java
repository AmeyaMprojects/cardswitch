package dev.cardswitch.vault;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;

import java.util.Set;

@RestController
@RequestMapping("/v1/tokens")
public class TokenController {

    public record TokenizeRequest(String pan) {}
    public record DetokenizeResponse(String pan) {}

    private final TokenService service;
    private final Set<String> detokenizeCallers;

    public TokenController(TokenService service, @Value("${vault.detokenize-callers}") Set<String> detokenizeCallers) {
        this.service = service;
        this.detokenizeCallers = detokenizeCallers;
    }

    @PostMapping
    public TokenService.TokenInfo tokenize(@RequestBody TokenizeRequest req) {
        try {
            return service.tokenize(req.pan());
        } catch (TokenService.InvalidCardException e) {
            throw new ResponseStatusException(HttpStatus.UNPROCESSABLE_ENTITY, e.getMessage());
        }
    }

    @GetMapping("/{token}")
    public TokenService.TokenInfo lookup(@PathVariable String token) {
        return service.lookup(token).orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));
    }

    /**
     * Returns the PAN. Only allowlisted callers may use it. The header is a
     * stand-in for mTLS client identity, which a real deployment would check.
     */
    @PostMapping("/{token}/detokenize")
    public DetokenizeResponse detokenize(@PathVariable String token,
                                         @RequestHeader(value = "X-Caller-Service", required = false) String caller) {
        if (caller == null || !detokenizeCallers.contains(caller)) {
            throw new ResponseStatusException(HttpStatus.FORBIDDEN, "caller may not detokenize");
        }
        return service.detokenize(token)
                .map(DetokenizeResponse::new)
                .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));
    }
}
