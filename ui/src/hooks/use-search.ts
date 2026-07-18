import { useMutation } from "@tanstack/react-query";
import { search } from "../api/client";
import type { SearchRequest } from "../api/types";

export function useSearch() {
  return useMutation({
    mutationFn: (payload: SearchRequest) => search(payload),
  });
}
