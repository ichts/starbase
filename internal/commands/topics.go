package commands

// Topics name what a command changed (writer.Tx.Touch). Every open page
// listens to its session's, its tab's and TopicAll (web.Server.stream); a
// command that changes nothing a page shows touches none.
const TopicAll = "all"

// SessionTopic is every page of one browser session: its preferences and
// who is signed in.
func SessionTopic(sid string) string { return "sid:" + sid }

// TabTopic is one loaded page: its tab state.
func TabTopic(tabID string) string { return "tab:" + tabID }
