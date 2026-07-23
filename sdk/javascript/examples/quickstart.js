import { RinClient } from "../src/index.js";

const client = new RinClient(process.env.RIN_URL, { token: process.env.RIN_TOKEN });

try {
  console.log(await client.health());
} catch (error) {
  console.error(`${error.code || "rin_error"}: ${error.message}`);
  process.exitCode = 1;
}
