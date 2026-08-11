import { RinControlClient } from "../src/index.js";

const client = new RinControlClient({ token: process.env.RIN_CONTROL_TOKEN });

try {
  console.log(await client.listWorlds());
} catch (error) {
  console.error(`${error.code || "rin_error"}: ${error.message}`);
  process.exitCode = 1;
}
