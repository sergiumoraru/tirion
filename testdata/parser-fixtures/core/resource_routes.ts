import defaultApi, { fetchResource as fetchResourceAlias, saveResource } from "./client"
import * as ResourceUtils from "./resource-utils"

type ResourceEnvelope<T extends ResourceRecord> = { item: T }

interface ResourceRecord {
  id: string
}

router.post("/resource/save", saveResource)

export async function saveResource(payload: ResourceEnvelope<ResourceRecord>) {
  const normalized = [payload.item].map((item) => ResourceUtils.normalize(item))
  return saveResource(normalized[0])
}

export function routeWithInlineHandler() {
  router.get("/resource/:id", (req, res) => {
    return res.json(req.params.id)
  })
}
