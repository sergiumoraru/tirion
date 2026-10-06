package example.catalog;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/catalog")
public class CatalogController {
    @GetMapping("/items")
    public String listItems(@RequestParam String region) {
        return loadItems(region);
    }

    private String loadItems(String region) {
        return region;
    }
}
