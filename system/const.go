package system

var Version = "develop"

// Features names what this NextHost build adds that NextHost checks for before it uses it, so it
// never has to compare version numbers. GET /api/system reports it as "features"; a build that
// leaves the field out has none of them.
//   - "shell": the staff shell socket at /api/servers/:server/shell
var Features = []string{"shell"}
