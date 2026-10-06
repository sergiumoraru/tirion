import axios from "axios";

export async function loadCatalog(region: string) {
  const response = await axios.get("/api/catalog/items", { params: { region } });
  return response.data;
}
