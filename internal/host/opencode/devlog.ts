import type { Plugin } from "@opencode/plugin"

export const DevLog: Plugin = async ({ $ }) => ({
  "tool.execute.before": async (input: any) => {
    await $`devlog check-feedback`.stdin(JSON.stringify({ ...input, cwd: process.cwd() }))
  },
  "tool.execute.after": async (input: any, output: any) => {
    const captureTools = new Set(["edit", "write", "bash"])
    if (captureTools.has(input.tool)) {
      await $`devlog capture`.stdin(JSON.stringify({ ...input, cwd: process.cwd() }))
    }
  },
  "chat.message": async (input: any) => {
    await $`devlog task-capture`.stdin(JSON.stringify({ ...input, cwd: process.cwd() }))
  },
  event: async ({ event }: any) => {
    if (event.type === "todo.updated") {
      await $`devlog task-tool-capture`.stdin(JSON.stringify({ ...event, cwd: process.cwd() }))
    }
  },
})
