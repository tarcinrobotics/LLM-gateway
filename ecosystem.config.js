module.exports = {
  apps: [
    {
      name: "llm-gateway",
      script: "./bin/server",
      cwd: "/home/tarcin/LLM-gateway",
      instances: 1,
      autorestart: true,
      watch: false,
      max_memory_restart: "1G",
      env: {
        CONFIG_PATH: "config.yaml"
      }
    }
  ]
};
