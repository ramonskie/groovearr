import { HardDriveDownload, Plus } from "lucide-react";

/**
 * Two distinct download intents — kept here so every surface names and icons
 * them identically instead of reusing an ambiguous "Download":
 *
 * - Local machine: the file already lives in the library; the browser streams
 *   it and saves it to the user's computer. Intent = Save.
 * - Library queue: a provider is asked to fetch the item and import it into
 *   the library. Intent = Add.
 */
export { HardDriveDownload as SaveToComputerIcon, Plus as AddToLibraryIcon };

/** Tooltip/aria label for a local-machine save action. */
export const saveToComputerTitle = (what: string): string =>
  `Save ${what} to your computer`;

/** Tooltip/aria label for a queue-to-library add action. */
export const addToLibraryTitle = (what: string): string =>
  `Add ${what} to your library`;
