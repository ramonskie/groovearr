import { useState } from "react";
import { Download, Music } from "lucide-react";
import { libraryAlbumDownloadUrl } from "../../api/client";
import type { Album } from "../../api/types";

interface AlbumCardProps {
  album: Album;
  artistName?: string;
}

export default function AlbumCard({ album, artistName }: AlbumCardProps) {
  const [imgError, setImgError] = useState(false);

  return (
    <div className="group cursor-pointer rounded-lg border border-slate-800 bg-slate-900 transition-colors hover:border-slate-700">
      {/* Cover image or fallback */}
      <div className="relative aspect-square w-full overflow-hidden rounded-t-lg bg-slate-800">
        {!imgError ? (
          <img
            src={`/api/covers/${album.id}`}
            alt={album.title}
            loading="lazy"
            onError={() => setImgError(true)}
            className="h-full w-full object-cover"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center">
            <Music size={36} className="text-slate-600" />
          </div>
        )}
        <a
          href={libraryAlbumDownloadUrl(album.id)}
          download
          onClick={(e) => e.stopPropagation()}
          title={`Download ${album.title}`}
          aria-label={`Download ${album.title}`}
          className="absolute right-2 top-2 flex h-8 w-8 items-center justify-center rounded-full bg-slate-900/80 text-slate-300 transition-colors hover:bg-slate-900 hover:text-white"
        >
          <Download size={15} />
        </a>
      </div>

      {/* Metadata */}
      <div className="p-3">
        <p className="truncate text-sm font-medium text-white" title={album.title}>
          {album.title}
        </p>
        {artistName && (
          <p className="truncate text-xs text-slate-400 mt-0.5" title={artistName}>
            {artistName}
          </p>
        )}
        {album.year && (
          <p className="mt-1 text-[11px] text-slate-500">{album.year}</p>
        )}
      </div>
    </div>
  );
}
