package api

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// The chat methods: watching chats (through their session's follower,
// chatfollow.go), sending, deciding, and attachments — staged on this
// machine while a session is parked.

// staged is where a parked session's attachments wait on this Mac.
func stagedDir() string { return filepath.Join(os.TempDir(), "pier-staged") }

const stagedPrefix = "staged:"

// splitAttachments separates attachments already in the session from
// those still on this Mac.
func splitAttachments(as []wire.Attachment) (inSession []wire.Attachment, local []string) {
	for _, a := range as {
		if p, ok := cutPrefix(a.Path, stagedPrefix); ok {
			local = append(local, p)
			continue
		}
		inSession = append(inSession, a)
	}
	return inSession, local
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}

// uploadStaged puts a running session's staged files in it.
func uploadStaged(ctx context.Context, be Chats, sess pier.Session, local []string) ([]wire.Attachment, error) {
	var out []wire.Attachment
	for _, p := range local {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		path, err := be.Upload(ctx, sess, filepath.Base(p), data)
		if err != nil {
			return nil, err
		}
		out = append(out, wire.Attachment{Name: filepath.Base(p), Path: path})
		os.Remove(p)
	}
	return out, nil
}

func init() {
	watchChanges("chats", "A session's chats: their state, what they wait on, their queues. Parked sessions answer from this machine's copy.",
		func(ctx context.Context, s *Server, p wire.SessionRef, sub *Sub[wire.ChatsChange]) (wire.ChatsSnapshot, error) {
			f, release, err := s.follower(p.ID)
			if err != nil {
				return wire.ChatsSnapshot{}, err
			}
			select {
			case <-f.ready:
			case <-time.After(chatReadyWait):
			case <-ctx.Done():
				release()
				return wire.ChatsSnapshot{}, ctx.Err()
			}
			snap, unsubscribe := f.subscribeList(sub.Emit)
			go func() {
				<-sub.Done()
				unsubscribe()
				release()
			}()
			return snap, nil
		})
	watchChanges("chat", "A chat's conversation: the newest items, then changes as the agent writes them (text streams in as deltas). Parked sessions answer from this machine's copy.",
		func(ctx context.Context, s *Server, p wire.ChatRef, sub *Sub[wire.ChatChange]) (wire.ChatSnapshot, error) {
			f, release, err := s.follower(p.SessionID)
			if err != nil {
				return wire.ChatSnapshot{}, err
			}
			select {
			case <-f.ready:
			case <-time.After(chatReadyWait):
			case <-ctx.Done():
				release()
				return wire.ChatSnapshot{}, ctx.Err()
			}
			// A live chat answers once its copy is caught up.
			if caught := f.caughtCh(p.ChatID); caught != nil {
				select {
				case <-caught:
				case <-time.After(chatReadyWait):
				case <-ctx.Done():
					release()
					return wire.ChatSnapshot{}, ctx.Err()
				}
			}
			snap, unsubscribe, err := f.subscribe(p.ChatID, sub.Emit)
			if err != nil {
				release()
				return wire.ChatSnapshot{}, err
			}
			go func() {
				<-sub.Done()
				unsubscribe()
				release()
			}()
			return snap, nil
		})
	handle("chat.history", "Older items of a chat, before the oldest one the client has.",
		func(ctx context.Context, s *Server, _ *Call, p wire.HistoryParams) (wire.HistoryPage, error) {
			s.chatMu.Lock()
			f := s.chats[p.SessionID]
			s.chatMu.Unlock()
			var page wire.HistoryPage
			var fromSession bool
			if f != nil {
				f.mu.Lock()
				if err := f.openFeed(p.ChatID); err != nil {
					f.mu.Unlock()
					return wire.HistoryPage{}, err
				}
				page, fromSession = f.feeds[p.ChatID].before(p.Before, p.Limit)
				f.mu.Unlock()
			} else {
				feed, err := openChatFeed(p.SessionID, p.ChatID)
				if err != nil {
					return wire.HistoryPage{}, err
				}
				page, fromSession = feed.before(p.Before, p.Limit)
				feed.close()
			}
			if !fromSession || len(page.Items) > 0 {
				return page, nil
			}
			// Older than this Mac keeps: ask the session, when it runs.
			var items []wire.ChatItem
			var more bool
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				if !pier.Running(sess) {
					return nil
				}
				items, more, err = be.ChatPage(ctx, sess, p.ChatID, p.Before, p.Limit)
				return err
			})
			if items == nil {
				items = []wire.ChatItem{}
			}
			return wire.HistoryPage{Items: items, More: more}, err
		})
	handle("chat.create", "Starts a chat in a session, once per chatId. A parked session is woken by a background job that starts it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatCreateParams) (wire.ChatCreated, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.ChatCreated{}, err
			}
			req := pier.ChatRequest{ID: p.ChatID, Agent: p.Agent, Model: p.Model, Effort: p.Effort, Mode: p.Mode, Title: p.Title}
			var local []string
			if p.Prompt != nil {
				prompt := *p.Prompt
				prompt.Attachments, local = splitAttachments(prompt.Attachments)
				req.Prompt = &prompt
			}
			if sess.State != pier.StateParked {
				if len(local) > 0 {
					up, err := uploadStaged(ctx, be, sess, local)
					if err != nil {
						return wire.ChatCreated{}, err
					}
					req.Prompt.Attachments = append(req.Prompt.Attachments, up...)
					local = nil
				}
				id, err := be.CreateChat(ctx, sess, req)
				if err == nil {
					s.overview.kick(fastAfterAction)
					return wire.ChatCreated{ChatID: id}, nil
				}
				if err != pier.ErrParked {
					return wire.ChatCreated{}, err
				}
			}
			if req.Prompt == nil {
				req.Prompt = &wire.ChatPrompt{}
			}
			if req.ID == "" || req.Prompt.ID == "" {
				return wire.ChatCreated{}, invalidParams("a parked session's new chat needs chatId and a prompt with an id")
			}
			job, err := be.StartDeliver(sess, pier.Delivery{Chat: req, Prompt: *req.Prompt, Files: local})
			if err != nil {
				return wire.ChatCreated{}, err
			}
			s.overview.kick(fastAfterAction)
			w := wireJob(job)
			return wire.ChatCreated{ChatID: req.ID, Job: &w}, nil
		})
	handle("chat.send", "Sends a prompt to a chat, once per prompt id. A parked session is woken by a background job that delivers it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatSendParams) (wire.ChatSendResult, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.ChatSendResult{}, err
			}
			prompt := p.Prompt
			var local []string
			prompt.Attachments, local = splitAttachments(prompt.Attachments)
			if sess.State != pier.StateParked {
				if len(local) > 0 {
					up, err := uploadStaged(ctx, be, sess, local)
					if err != nil {
						return wire.ChatSendResult{}, err
					}
					prompt.Attachments = append(prompt.Attachments, up...)
					local = nil
				}
				err = be.SendPrompt(ctx, sess, p.ChatID, prompt, p.Mode)
				if err != pier.ErrParked {
					return wire.ChatSendResult{}, err
				}
			}
			job, err := be.StartDeliver(sess, pier.Delivery{Chat: pier.ChatRequest{ID: p.ChatID}, Prompt: prompt, Files: local, Mode: p.Mode})
			if err != nil {
				return wire.ChatSendResult{}, err
			}
			s.overview.kick(fastAfterAction)
			w := wireJob(job)
			return wire.ChatSendResult{Job: &w}, nil
		})
	handle("chat.dequeue", "Takes back a prompt still waiting in a chat's queue.",
		func(ctx context.Context, s *Server, _ *Call, p wire.DequeueParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error { return be.Dequeue(ctx, sess, p.ChatID, p.PromptID) })
		})
	handle("chat.interrupt", "Stops a chat's turn.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error { return be.Interrupt(ctx, sess, p.ChatID) })
		})
	handle("chat.decide", "Answers what a chat waits on: a tool use, its questions, its plan.",
		func(ctx context.Context, s *Server, _ *Call, p wire.DecideParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.Decide(ctx, sess, p.ChatID, p.RequestID, pier.Decision{
					Behavior: p.Behavior, Message: p.Message, Answers: p.Answers, Notes: p.Notes, Mode: p.Mode,
				})
			})
		})
	handle("chat.configure", "Changes a chat's model, effort, permission mode or title as it goes on.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatConfigureParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.ConfigureChat(ctx, sess, p.ChatID, pier.ChatConfig{Model: p.Model, Effort: p.Effort, Mode: p.Mode, Title: p.Title})
			})
		})
	handle("chat.close", "Stops a chat and puts it away.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error { return be.CloseChat(ctx, sess, p.ChatID) })
		})
	handle("chat.takeover", "Opens a terminal window in the session that continues a chat in its agent's own interface; the chat waits, and takes back what happens there when it exits. The result is the window to open a terminal tab on.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatRef) (wire.WindowRef, error) {
			var w wire.WindowRef
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				w.WindowID, err = be.ChatWindow(ctx, sess, p.ChatID, true)
				return err
			})
			return w, err
		})
	handle("chats.window", "Opens the session's chats, live and read-only, in a terminal window of their own (made if missing); the result is the window to open a terminal tab on.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (wire.WindowRef, error) {
			var w wire.WindowRef
			err := s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) (err error) {
				w.WindowID, err = be.ChatWindow(ctx, sess, "", false)
				return err
			})
			return w, err
		})
	handle("agents.models", "What an agent offers — models with their efforts, permission modes — as a running session's agent says, else as last heard.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ModelsParams) (wire.AgentModels, error) {
			be, err := s.backend()
			if err != nil {
				return wire.AgentModels{}, err
			}
			if p.Agent != "claude" && p.Agent != "codex" {
				return wire.AgentModels{}, invalidParams("agent is claude or codex")
			}
			var sess *pier.Session
			if p.SessionID != "" {
				if got, err := s.overview.session(ctx, p.SessionID); err == nil && pier.Running(got) {
					sess = &got
				}
			}
			return be.Models(ctx, sess, p.Agent)
		})
	handle("files.upload", "Puts a file attached to a prompt into a session; a parked session's waits on this Mac until the prompt wakes it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.UploadParams) (wire.Attachment, error) {
			data, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				return wire.Attachment{}, invalidParams("data isn't base64")
			}
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.Attachment{}, err
			}
			name := filepath.Base(p.Name)
			if !pier.Running(sess) {
				dir := filepath.Join(stagedDir(), pier.NewID())
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return wire.Attachment{}, err
				}
				local := filepath.Join(dir, name)
				if err := os.WriteFile(local, data, 0o600); err != nil {
					return wire.Attachment{}, err
				}
				return wire.Attachment{Name: name, Path: stagedPrefix + local, Mime: p.Mime}, nil
			}
			path, err := be.Upload(ctx, sess, name, data)
			return wire.Attachment{Name: name, Path: path, Mime: p.Mime}, err
		})
	handle("files.list", "The paths in a session's checkout, tracked and untracked (ignored ones left out). A session that can't be read answers with the list this machine kept (asOf).",
		func(ctx context.Context, s *Server, _ *Call, p wire.FileListParams) (wire.FileList, error) {
			out := wire.FileList{Paths: []string{}}
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return out, err
			}
			var paths []string
			if readable(be, sess) {
				paths, out.Truncated, err = be.ListFiles(ctx, sess)
			} else if kept, truncated, at, ok := pier.CachedFiles(sess.ID); ok {
				paths, out.Truncated, out.AsOf = kept, truncated, asOf(at)
			} else {
				err = unreadable(be, sess)
			}
			if paths != nil {
				out.Paths = paths
			}
			return out, err
		})
}
