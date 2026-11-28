# Brain Hub desktop 0.1.16

- **Start a session on another machine (macOS).** New Session on a peer machine, for example the Studio while the laptop hub is selected, now goes to that machine's hub. Before, it failed with "has no session agent".
- **Use the route that answers.** When the app knows two routes to a machine (Tailscale and a home-network tunnel), New Session and Move use the one that answers.

Builds: native universal macOS app, Linux (AppImage, deb, rpm) and Windows (msi, exe) desktop packages.
