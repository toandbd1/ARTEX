import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "ARTEX",
  version: packageJson.version,
  copyright: `© ${currentYear}, ARTEX.`,
  meta: {
    title: "ARTEX — Bảng điều khiển kiểm thử xâm nhập tự động",
    description: "Bảng điều khiển hệ thống kiểm thử xâm nhập tự động vận hành bởi LLM",
  },
};
