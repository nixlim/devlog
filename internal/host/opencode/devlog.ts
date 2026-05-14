import type { Plugin } from "@opencode-ai/plugin"

export const DevLog: Plugin = async ({ $ }) => ({
  "tool.execute.before": async (input: any) => {
    const payload = JSON.stringify({ ...input, cwd: process.cwd() })
    const feedback = await $`echo ${payload} | devlog check-feedback`.text()
    if (feedback.trim()) {
      throw new Error(feedback)
    }
  },
  "tool.execute.after": async (input: any, output: any) => {
    const captureTools = new Set(["edit", "write", "bash"])
    if (captureTools.has(input.tool)) {
      const payload = JSON.stringify({ ...input, cwd: process.cwd() })
      await $`echo ${payload} | devlog capture`
    }
  },
  "chat.message": async (input: any, output: any) => {
    const parts = output?.parts ?? []
    const content = parts
      .filter((p: any) => p.type === "text" && !p.ignored)
      .map((p: any) => p.text)
      .join("\n")
    const payload = JSON.stringify({
      content,
      sessionId: input.sessionID ?? input.sessionId,
      cwd: process.cwd(),
    })
    await $`echo ${payload} | devlog task-capture`
  },
  event: async ({ event }: any) => {
    if (event.type === "todo.updated") {
      const payload = JSON.stringify({ ...event, cwd: process.cwd() })
      await $`echo ${payload} | devlog task-tool-capture`
    }
  },
})
