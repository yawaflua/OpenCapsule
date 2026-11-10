#!/usr/bin/env python3
# Copyright 2026 The Marusya-Gemini Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Uploads plugin archives to the plugin catalog (registry/).

For every ZIP it reads plugin.json, creates the project (slug = id) if you
have none with that slug, uploads the version from plugin.json unless it is
already there and submits the project for review. With --publish (needs the
admin role) it publishes the project right away.

    python3 scripts/catalog-upload.py --email you@example.com bin/plugins/*.zip

The password is asked for, or taken from $MARUSYA_CATALOG_PASSWORD.
Standard library only.
"""

import argparse
import base64
import getpass
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile


class APIError(Exception):
    def __init__(self, status, message):
        super().__init__("HTTP %d: %s" % (status, message))
        self.status = status


class Catalog:
    def __init__(self, url):
        self.url = url.rstrip("/")
        self.token = ""

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.url + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        # Cloudflare's browser check refuses the default Python-urllib agent.
        req.add_header("User-Agent", "marusya-catalog-upload/1.0 (+https://github.com/yawaflua/Gemini-for-Capsula)")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=120) as res:
                raw = res.read()
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                msg = json.loads(raw).get("message") or raw.decode(errors="replace")
            except ValueError:
                msg = raw.decode(errors="replace")
            raise APIError(e.code, msg.strip()) from None
        return json.loads(raw) if raw.strip() else {}

    def login(self, email, password):
        res = self.call("POST", "/v1/auth/login", {"email": email, "password": password})
        self.token = res["tokens"]["access_token"]
        return res["user"]

    def my_projects(self):
        out, token = {}, ""
        while True:
            q = {"pageSize": 100}
            if token:
                q["pageToken"] = token
            res = self.call("GET", "/v1/users/me/projects?" + urllib.parse.urlencode(q))
            for p in res.get("projects") or []:
                out[p["slug"]] = p
            token = res.get("next_page_token") or ""
            if not token:
                return out


def manifest(path):
    """Returns plugin.json from the archive root or its single folder."""
    with zipfile.ZipFile(path) as z:
        names = [n for n in z.namelist() if n.rstrip("/").split("/")[-1] == "plugin.json"]
        names.sort(key=lambda n: n.count("/"))
        if not names or names[0].count("/") > 1:
            raise ValueError("no plugin.json in the archive root")
        return json.loads(z.read(names[0]))


def upload(cat, path, projects, args):
    man = manifest(path)
    pid, version = man["id"], man.get("version") or ""
    if not version:
        raise ValueError("plugin.json has no version")
    name = man.get("name") or pid
    description = man.get("description") or name

    project = projects.get(pid)
    if project is None:
        project = cat.call("POST", "/v1/projects", {
            "slug": pid, "name": name, "description": description, "git_url": args.git_url,
        })
        projects[pid] = project
        print("  проект создан")

    versions = cat.call("GET", "/v1/projects/%s/versions" % project["id"]).get("versions") or []
    if any(v.get("version") == version for v in versions):
        print("  версия %s уже есть" % version)
    else:
        with open(path, "rb") as f:
            data = base64.b64encode(f.read()).decode()
        cat.call("POST", "/v1/projects/%s/versions" % project["id"], {
            "version": version,
            "description": description,
            "changelog": args.changelog,
            "readme": "",
            "tag": "VERSION_TAG_RELEASE",
            "metadata": {
                "slug": pid, "description": description,
                "licenses": [args.license], "authors": args.author,
                "abi_version": "1", "api_version": "1",
            },
            "archive": {"content_type": "application/zip", "data": data},
        })
        print("  версия %s загружена" % version)

    status = cat.call("GET", "/v1/projects/id/%s" % project["id"]).get("status", "")
    if status in ("PROJECT_STATUS_DRAFT", "PROJECT_STATUS_REJECTED", "", None):
        cat.call("POST", "/v1/projects/%s:submit" % project["id"], {"comment": args.comment})
        status = "PROJECT_STATUS_PENDING_REVIEW"
        print("  отправлен на модерацию")
    if args.publish and status != "PROJECT_STATUS_PUBLISHED":
        cat.call("POST", "/v1/admin/projects/%s:set-status" % project["id"],
                 {"status": "PROJECT_STATUS_PUBLISHED", "reason": args.comment})
        print("  опубликован")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("archives", nargs="+", help="ZIP archives with plugin.json")
    ap.add_argument("--registry", default=os.environ.get("MARUSYA_PLUGIN_REGISTRY", "https://marusya.ywfl.dev"))
    ap.add_argument("--email", required=True)
    ap.add_argument("--git-url", default="https://github.com/yawaflua/Gemini-for-Capsula")
    ap.add_argument("--license", default="Apache-2.0")
    ap.add_argument("--author", action="append", default=None, help="repeatable; default: your nickname")
    ap.add_argument("--changelog", default="Первая версия.")
    ap.add_argument("--comment", default="Пример из репозитория Маруси.")
    ap.add_argument("--publish", action="store_true", help="publish at once (admin only)")
    args = ap.parse_args()

    password = os.environ.get("MARUSYA_CATALOG_PASSWORD") or getpass.getpass("Пароль от %s: " % args.registry)
    cat = Catalog(args.registry)
    try:
        user = cat.login(args.email, password)
    except APIError as e:
        sys.exit("вход не удался: %s" % e)
    args.author = args.author or [user.get("username") or args.email]
    projects = cat.my_projects()

    failed = 0
    for path in args.archives:
        print(os.path.basename(path))
        try:
            upload(cat, path, projects, args)
        except (APIError, ValueError, OSError, KeyError, zipfile.BadZipFile) as e:
            failed += 1
            print("  ошибка: %s" % e)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
