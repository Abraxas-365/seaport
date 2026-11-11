Revisé las dos entradas nuevas contra el código. Dos son correctas y hay un punto que corregir.

**Correcto**
- **Contents: write**: el manifest pide `"contents": "write"` (`internal/dock/adapters/dockgithub/http.go:190-194`), y `commitFiles` usa la Git Data API (blobs, trees, commits, refs) con el token de instalación (`dockgithub/files.go:26-92`). Sin ese permiso, GitHub rechaza el `POST /git/blobs` y el envío falla.
- **Rama `seaport-files`**: es `FilesBranch = "seaport-files"` (`files.go:18`), una rama huérfana que se crea en el primer uso, con los archivos bajo `pr-<n>/<message id>/`.
- **`this port keeps no files`**: es `shipsvc.keepFiles`, un `errx.Business` (`internal/ship/shipsvc/media.go:131-132`) que se devuelve cuando no hay sealer (sin `SEAPORT_SECRET_KEY` o con `SEAPORT_BLOB_URL=off`), antes de guardar el ship. El mensaje completo es `this port keeps no files (SEAPORT_SECRET_KEY and SEAPORT_BLOB_URL)`. Citar solo el prefijo coincide con `docs/reference/mcp-tools.md:70` y con el e2e (`files_e2e_test.go:144`).

**A corregir**
1. **"or the commit's folder" no rompe los enlaces.** Los enlaces apuntan al SHA del commit (`.../blob/<commit>/pr-<n>/...`, `files.go:100`; el comentario de `FilesBranch` dice "so a link keeps working after later files"). Si alguien borra la carpeta en un commit posterior, el commit original sigue existiendo y el enlace sigue funcionando. Solo se rompe si el commit deja de ser alcanzable, es decir, si se borra la rama o se reescribe con force-push. Incluso así, GitHub puede seguir sirviendo el commit por su SHA hasta que pase su recolector de basura, así que es mejor decir que el enlace "puede" dar 404. Propuesta:
   > - **File links in a comment give `404`.** The reader has no access to the repository, or someone deleted or force-pushed the `seaport-files` branch (links point at the commit, so deleting a folder in a later commit doesn't break them; a commit no branch reaches can disappear).

   `docs/guides/messages-and-images.md:69` tiene la misma imprecisión ("delete old folders … the comments' links then stop working"). Sugiero corregirla en este PR también.

**Detalles menores (opcionales)**
2. "an App created before it asked for that": el manifest pide `contents: write` desde el primer commit del canal GitHub (`22d9c7c`). Ningún App creado por Seaport ha existido sin ese permiso. El caso real es un App al que alguien le quitó o bajó el permiso a mano en su configuración, o una instalación que todavía no aceptó un cambio de permisos. Podría decir algo como: "If someone lowered it in the App's settings, set it back; an installation then shows a pending permission request an owner must accept."
3. En la entrada de blob storage convendría nombrar la causa y enlazarla, como en messages-and-images.md: "A port without blob storage (`SEAPORT_BLOB_URL=off` or no secret key) refuses files (`this port keeps no files`); see [Files agents send](messages-and-images.md#files-agents-send)."

Con el punto 1 corregido, apruebo.
