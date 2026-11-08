package seed

import (
	"fmt"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// chatgpt and claude: conversations as message threads (user/assistant turns
// under one ThreadExternalID, each turn parented on the previous one), plus
// Claude's projects with their documents. Shapes follow providers/chatgpt and
// providers/claude ("ChatGPT · <title>" / "Claude · <title>" titles).
func chatgpt(g *gen) { aiChat(g, "chatgpt", "ChatGPT", chatgptTopics, "user", "assistant", 2023) }
func claude(g *gen)  { aiChat(g, "claude", "Claude", claudeTopics, "human", "assistant", 2024) }

func aiChat(g *gen, src, label string, topics []string, userRole, assistantRole string, since int) {
	models := map[string][]string{
		"chatgpt": {"gpt-4o", "gpt-4o", "gpt-4.1", "o3", "gpt-5"},
		"claude":  {"claude-3-5-sonnet-20241022", "claude-sonnet-4-20250514", "claude-opus-4-1", "claude-sonnet-4-5"},
	}[src]
	for c, topic := range topics {
		turns := 4 + g.rng.Intn(9) // 4–12 messages per conversation
		start := g.since(since)
		thread := fmt.Sprintf("%08x-0000-4000-8000-%012x", hash64(topic)&0xffffffff, c)
		model := models[min(len(models)-1, int(g.now.Sub(start).Hours()/24/365*float64(len(models))/3))]
		prev := ""
		at := start
		for m := 0; m < turns; m++ {
			role, body := userRole, g.pick(userLines)
			if m == 0 {
				body = topic + ". " + body
			}
			if m%2 == 1 {
				role, body = assistantRole, g.pick(assistantLines)
				at = at.Add(time.Duration(g.between(4, 40)) * time.Second)
			} else if m > 0 {
				at = at.Add(g.minutes(0, 6))
			}
			id := fmt.Sprintf("msg:%08x-%04d", hash64(thread)&0xffffffff, m)
			ev := domain.Event{
				SourceID: src, ExternalID: id, Type: src + ".message", Category: domain.CategoryMessage,
				OccurredAt: at, Actor: role, Title: label + " · " + topic, Body: body,
				ThreadExternalID: thread, ParentExternalID: prev,
				Metadata: map[string]any{"conversation": topic, "model": model},
			}
			if src == "chatgpt" {
				ev.Metadata["content_type"], ev.Metadata["on_current_branch"], ev.Metadata["node_id"] = "text", true, fmt.Sprintf("n%d", m)
				if role == assistantRole {
					ev.Metadata["status"] = "finished_successfully"
				}
				if m == 0 && g.chance(0.15) {
					ev.Metadata["content_type"] = "multimodal_text"
					ev.Media = []domain.Media{{URL: "file-service://file-" + short(id)[:12], MIME: "image/*"}}
				}
			} else {
				ev.Metadata["block_types"] = []string{"text"}
				if role == assistantRole && g.chance(0.2) {
					ev.Metadata["block_types"] = []string{"text", "tool_use", "tool_result", "text"}
				}
			}
			prev = id[4:]
			g.add(ev)
		}
	}

	switch src {
	case "chatgpt":
		g.add(g.unknown(src, "user.json", `{"id": "user-seedDEMO0000000000000001", "email": "jane.doe@example.com", "chatgpt_plus_user": true, "phone_number": null}`, 143))
		g.add(g.unknown(src, "shared_conversations.json", `[{"id": "6e4f…", "conversation_id": "…", "title": "Explain keyset pagination like I'm five", "is_anonymous": true}]`, 812))
		g.add(g.unknown(src, "message_feedback.json", `[{"id": "fb-1", "conversation_id": "…", "rating": "thumbs_up", "create_time": "2025-05-11T19:02:44Z"}]`, 4416))
	case "claude":
		for i, p := range claudeProjects {
			at := g.since(2024)
			pid := fmt.Sprintf("project:%08x-0000-4000-8000-%012x", hash64(p.Name)&0xffffffff, i)
			ev := domain.Event{
				SourceID: src, ExternalID: pid, Type: "claude.project", Category: domain.CategoryContent,
				OccurredAt: at, Actor: "self", Title: "Project: " + p.Name, Body: p.Desc, ThreadExternalID: pid,
				Metadata: map[string]any{"docs": 0, "description": p.Desc, "private": true, "updated_at": rfc3339(at.Add(72 * time.Hour))},
			}
			docs := claudeProjectDocs[i*3 : min(len(claudeProjectDocs), i*3+3)]
			ev.Metadata["docs"] = len(docs)
			g.add(ev)
			for j, d := range docs {
				doc := domain.Event{
					SourceID: src, ExternalID: fmt.Sprintf("project_doc:%08x-%02d", hash64(p.Name)&0xffffffff, j), Type: "claude.project_doc", Category: domain.CategoryContent,
					OccurredAt: at.Add(time.Duration(j+1) * 25 * time.Minute), Actor: "self", Title: d.Title + " · " + p.Name, Body: d.Body,
					ThreadExternalID: pid, ParentExternalID: pid,
					Metadata: map[string]any{"project": p.Name, "filename": d.Title},
				}
				g.add(doc)
			}
		}
		g.add(g.unknown(src, "users.json", `[{"uuid": "seed0000-0000-4000-8000-000000000001", "full_name": "Jane Doe", "email_address": "jane.doe@example.com", "verified_phone_number": null}]`, 171))
		g.add(g.unknown(src, "memories.json", `[{"uuid": "…", "conversation_uuid": "…", "content": "Prefers Go examples over pseudocode; runs a self-hosted archive app."}]`, 2290))
	}
}
