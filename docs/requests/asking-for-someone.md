---
title: Ask for someone through the API
description: Let an app with one API key, such as a Jellyfin plugin, file each request under the right person.
level: Setup
---

An app that asks for many people with one API key can name the person each call counts as. A Jellyfin plugin does this: every Jellyfin user's request lands under their own HolyArrs name, with their own limits.

Without it, every request made with a key counts as the person who made the key.

## What you need

- An API key made by the owner or an admin, with the write scope. Make one under **Settings > API > API keys**. See [The HolyArrs API](../dev/api.md).
- Each person linked to their Jellyfin account. A person is linked once they sign in with Jellyfin, or after you import your Jellyfin server's people. See [Sign-in](../users/sign-in.md).

## Name the person

Send the person's Jellyfin user id in the `X-HolyArrs-User` header, next to the key:

```
curl -X POST http://your-server:6262/api/v1/asks \
  -H "X-Api-Key: <your key>" \
  -H "X-HolyArrs-User: jellyfin:<Jellyfin user id>" \
  -H "Content-Type: application/json" \
  -d '{"id": "tmdb:693134"}'
```

The id may be written with or without dashes, in upper or lower case.

The call then runs as that person. Their own permissions, weekly limit and age-rating limit apply. A request that would wait for approval when they ask in HolyArrs waits here too.

History shows the request under the person's name, with the label of the key it came through.

## Where it works

| Call | What it does for the named person |
|---|---|
| `POST /api/v1/asks` | Asks for a title |
| `GET /api/v1/asks/limit` | Reads what is left of their weekly limit |
| `GET /api/v1/asks` | Lists the requests they may see |
| `GET /api/v1/asks/{id}` | Reads one of them |
| `DELETE /api/v1/asks/{id}` | Takes back a request of theirs |

Every other call refuses the header. Browser sessions ignore it.

## If a call is refused

Each refusal has a `code` an app can read.

| Status | `code` | Cause | Fix |
|---|---|---|---|
| 400 | `as_user_route` | The header was sent on a call that does not take it | Send it only on the calls above |
| 400 | `as_user_value` | The value is not `jellyfin:` and a Jellyfin user id | Check the id |
| 403 | `as_user_key` | The key was not made by the owner or an admin | Make the key as the owner or an admin |
| 404 | `user_not_linked` | No HolyArrs person is linked to that Jellyfin user | Have them sign in with Jellyfin, or import your Jellyfin people |
| 403 | `user_off` | The linked person's account is turned off | Turn the account on under **Settings > Users and roles** |

A refused call files nothing. It never counts as the key's owner instead.

## Related

- [Ask for a title](asking.md)
- [Weekly limits and Request rules](settings.md)
- [The HolyArrs API](../dev/api.md)
