package alerts

import (
	"fmt"
	"net/url"

	"monitor/internal/store"
)

// Mail goes to everyone who may see the project (ruling 10): every admin,
// and the members given the project. It is plain text; the texts name
// BASE_URL, never the tool's name, which only the dashboard knows.

// footer ends every project mail: why you get it, and where to change it.
func (n *Notifier) footer(p store.Project) string {
	return fmt.Sprintf("\n--\nYou get this mail because you can see %s on %s.\nChoose which alerts %s sends: %s\n",
		p.Name, n.baseURL, p.Name, n.link("/projects/%d/alerts", p.ID))
}

// adminFooter ends every mail about the tool itself.
func (n *Notifier) adminFooter() string {
	return fmt.Sprintf("\n--\nYou get this mail because you are an admin of %s.\nStatus: %s\n", n.baseURL, n.link("/settings/status"))
}

// host is BASE_URL's host, which names this installation in admin subjects.
func (n *Notifier) host() string {
	if u, err := url.Parse(n.baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return n.baseURL
}
