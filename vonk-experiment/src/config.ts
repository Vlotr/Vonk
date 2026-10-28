import { readFileSync } from "node:fs";
import type { Config, Testpunt } from "./types";

try {
  process.loadEnvFile();
} catch {
  // Geen .env: dan komen de sleutels uit de omgeving.
}

export function leesJson<T>(pad: string): T {
  return JSON.parse(readFileSync(pad, "utf8")) as T;
}

export const config = leesJson<Config>("config.json");
export const testpunten = leesJson<Testpunt[]>("testpunten.json");
