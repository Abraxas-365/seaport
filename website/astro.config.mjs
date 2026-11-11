import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'
import { unified } from '@astrojs/markdown-remark'
import { remarkDocs, repository, site, base } from './scripts/docs.mjs'
import { sidebar } from './src/sidebar.mjs'

export default defineConfig({
  // GitHub Pages serves the project at https://abraxas-365.github.io/seaport/ (SITE_BASE=/seaport in CI).
  site,
  base,
  trailingSlash: 'always',
  markdown: { processor: unified({ remarkPlugins: [remarkDocs] }) },
  integrations: [starlight({
    title: 'Seaport',
    description: 'A port for your AI agents: messages arrive at docks, customs decides who may wake an agent, and your crew answers. Tutorials, guides and reference.',
    logo: { src: './src/assets/seaport-mark.svg' },
    social: [{ icon: 'github', label: 'GitHub', href: repository }],
    customCss: ['./src/styles/theme.css'],
    expressiveCode: { themes: ['github-dark-dimmed', 'github-light'], styleOverrides: { borderRadius: '0.5rem' } },
    sidebar,
  })],
})
