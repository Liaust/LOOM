# Automation Notes

This example models the temporary standalone Gmail automation as a LOOM project:

1. An external system receives an email.
2. The external system extracts a URL.
3. The external system sends a direct event to LOOM.
4. LOOM calls `main@automation-email-url.fetch_url`.
5. The external system receives accepted/completed confirmation and tags the email.
