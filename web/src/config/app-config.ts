import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "Infinidatum PenTest",
  version: packageJson.version,
  copyright: `© ${currentYear}, Infinidatum.`,
  meta: {
    title: "Infinidatum PenTest — Autonomous Penetration Testing Console",
    description: "Console for the LLM-driven autonomous penetration testing system by Infinidatum",
  },
};
