const createClient = () => axios.create({});

function buildClient() {
  return axios.create({});
}

export function makeCalls() {
  const axiosClient = axios.create({});
  const apiClient = createClient();
  const otherClient = buildClient();
  const method = "post";
  axios({ url: "/api/foo", method: "post" });
  axios.request({ url: "/api/req", method: "PUT" });
  fetch("/api/fetch", { method: "patch" });
  axios?.get("/api/optional");
  axios({ url: "/api/short" });
  axiosClient["post"]("/api/quoted");
  axiosClient[method]("/api/dynamic");
  apiClient.get("/api/factory");
  otherClient.post("/api/factory2");
  got("/api/got-default");
  got.post("/api/got-post");
  ky("/api/ky-default", { method: "DELETE" });
  ky.patch("/api/ky-patch");
  $.ajax({ url: "/api/jquery", type: "DELETE" });
  $.get("/api/jquery-get");
}
