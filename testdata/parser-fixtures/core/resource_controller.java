package fixtures;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;

@RequestMapping("/api/resources")
public abstract class ResourceController<T extends Number> extends BaseController implements ResourcePort {
    @GetMapping("/{id}")
    public abstract ResourceResponse getResource(T id);

    @PostMapping("/save")
    public ResourceResponse saveResource(@RequestBody ResourceRequest request) {
        Runnable audit = () -> saveAudit(request);
        audit.run();
        return ResourceResponse.ok();
    }

    public String format(String raw) {
        return raw;
    }

    public String format(Integer raw) {
        return String.valueOf(raw);
    }

    protected <R extends Comparable<R>> R normalize(R input) {
        return input;
    }

    private void saveAudit(ResourceRequest request) {
    }
}

abstract class BaseController {
}

interface ResourcePort {
}

class ResourceRequest {
}

class ResourceResponse {
    static ResourceResponse ok() {
        return new ResourceResponse();
    }
}
