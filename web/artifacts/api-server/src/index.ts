import app from "./app";
import { logger } from "./lib/logger";

// Dev-bridge default so `pnpm dev` works without Replit-style env injection.
// An ambient PORT of "0" (exported by some sandboxed shells) is treated as unset.
const rawPort =
  process.env["PORT"] && process.env["PORT"] !== "0"
    ? process.env["PORT"]
    : "19288";

const port = Number(rawPort);

if (Number.isNaN(port) || port <= 0) {
  throw new Error(`Invalid PORT value: "${rawPort}"`);
}

app.listen(port, (err) => {
  if (err) {
    logger.error({ err }, "Error listening on port");
    process.exit(1);
  }

  logger.info({ port }, "Server listening");
});
