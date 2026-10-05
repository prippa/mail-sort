document.addEventListener('alpine:init', function () {
  Alpine.data('mailsorter', function () {
    return {
      token: '',
      boot: null,
      screen: 'wizard',
      step: 0,
      error: '',
      notice: '',
      draft: blankDraft(),
      categories: [],
      rules: [],
      classifiers: [],
      sample: { from: '', to: '', subject: '', body: '' },
      fields: [],
      result: null,
      previewNote: '',
      connection: { folders: [] },
      pending: '',
      report: { rows: [], note_key: '' },
      runForm: {
        profile: '',
        folder: 'INBOX',
        limit: 200,
        unread: false,
        since: '',
        include_flagged: false,
        include_drafts: false,
        copy_only: false
      },
      overrideUID: '',
      overrideCategory: 'keep_in_inbox',
      activity: { runs: [] },
      settings: { language: 'auto', privacy: blankPrivacy() },
      consentOpen: false,
      afterConsent: '',

      init: function () {
        var hash = location.hash.replace(/^#/, '')
        if (hash) {
          sessionStorage.setItem('mailsorter-token', hash)
          document.cookie = 'mailsorter_session=' + encodeURIComponent(hash) + '; Path=/; SameSite=Strict'
          history.replaceState(null, '', location.pathname)
        }
        this.token = sessionStorage.getItem('mailsorter-token') || ''
        if (!this.token) {
          return
        }
        this.load()
      },

      text: function (key) {
        if (!this.boot || !this.boot.strings || !this.boot.strings[key]) {
          return key
        }
        return this.boot.strings[key]
      },

      api: async function (method, path, body) {
        var headers = { 'X-MailSorter-Token': this.token }
        var opts = { method: method, headers: headers, credentials: 'same-origin' }
        if (body !== undefined) {
          headers['Content-Type'] = 'application/json'
          opts.body = JSON.stringify(body)
        }
        var res = await fetch(path, opts)
        var data = {}
        try {
          data = await res.json()
        } catch (err) {
          data = {}
        }
        if (!res.ok) {
          var error = new Error(data.error || ('HTTP ' + res.status))
          error.status = res.status
          error.consent = !!data.consent
          throw error
        }
        return data
      },

      load: async function () {
        try {
          this.boot = await this.api('GET', '/api/bootstrap')
          this.copyBoot()
          if (!this.runForm.profile && this.boot.profiles && this.boot.profiles.length) {
            this.runForm.profile = this.boot.profiles[0].name
          }
          if (this.screen === 'wizard' && this.boot.profiles && this.boot.profiles.length && this.step === 0) {
            this.screen = 'accounts'
          }
          this.error = ''
        } catch (err) {
          this.error = err.message
        }
      },

      go: function (name) {
        this.screen = name
        this.notice = ''
        this.error = ''
      },

      pickProvider: function (id) {
        this.draft = blankDraft()
        this.draft.provider = id
        this.step = 1
        this.connection = { folders: [] }
      },

      presetLabel: function (preset) {
        if (preset.id === 'custom') {
          return this.text('wizard.custom')
        }
        return preset.name
      },

      selectedPreset: function () {
        var list = (this.boot && this.boot.presets) || []
        for (var i = 0; i < list.length; i++) {
          if (list[i].id === this.draft.provider) {
            return list[i]
          }
        }
        return null
      },

      canUsePassword: function () {
        var preset = this.selectedPreset()
        if (!preset || !preset.auth) {
          return false
        }
        return preset.auth.indexOf('password') !== -1
      },

      needsHost: function () {
        return this.draft.provider === 'custom'
      },

      hostChoices: function () {
        var preset = this.selectedPreset()
        if (!preset || !preset.hosts) {
          return []
        }
        return preset.hosts
      },

      hostLabel: function (host) {
        return host.id + ' ' + host.host
      },

      presetNote: function () {
        var preset = this.selectedPreset()
        if (!preset) {
          return ''
        }
        return preset.note || preset.username_hint || ''
      },

      profilePayload: function () {
        return {
          name: this.draft.name,
          provider: this.draft.provider,
          host: this.draft.host,
          host_id: this.draft.host_id,
          port: Number(this.draft.port) || 0,
          security: this.draft.security,
          username: this.draft.username,
          password_env: this.draft.password_env,
          email: this.draft.email,
          discover: !!this.draft.discover,
          max_chars: Number(this.draft.max_chars) || 0
        }
      },

      saveAccount: async function () {
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/profiles', this.profilePayload())
          this.copyBoot()
          this.notice = this.text('accounts.saved')
        })
      },

      testAccount: async function () {
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/profiles', this.profilePayload())
          this.copyBoot()
          this.connection = await this.api('POST', '/api/profiles/test', { name: this.draft.name })
          this.notice = this.text('accounts.connected')
        })
      },

      testNamed: async function (name) {
        await this.guard(async function () {
          this.connection = await this.api('POST', '/api/profiles/test', { name: name })
          this.notice = this.text('accounts.connected')
          this.screen = 'accounts'
        })
      },

      connectionSummary: function () {
        if (!this.connection || !this.connection.host) {
          return ''
        }
        return this.connection.host + ':' + this.connection.port + ' ' + this.connection.security +
          ' MOVE=' + this.connection.move + ' UIDPLUS=' + this.connection.uidplus
      },

      removeAccount: async function (name) {
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/profiles/delete', { name: name })
          this.copyBoot()
          this.notice = this.text('accounts.removed')
        })
      },

      useStarter: async function () {
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/categories/starter')
          this.copyBoot()
          this.notice = this.text('wizard.use_starter')
        })
      },

      addCategory: function () {
        this.categories.push({
          key: '',
          name: '',
          name_ru: '',
          description: '',
          examples: [],
          folder: '',
          action: 'move',
          min_confidence: 0,
          reserved: false
        })
      },

      removeCategory: function (cat) {
        var next = []
        for (var i = 0; i < this.categories.length; i++) {
          if (this.categories[i] !== cat) {
            next.push(this.categories[i])
          }
        }
        this.categories = next
      },

      saveCategories: async function () {
        var user = []
        for (var i = 0; i < this.categories.length; i++) {
          if (!this.categories[i].reserved) {
            user.push(this.categories[i])
          }
        }
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/categories', { categories: user, rules: this.rules })
          this.copyBoot()
          this.notice = this.text('action.save')
        })
      },

      ruleLine: function (rule) {
        return (rule.name || rule.from || rule.from_domain || rule.subject || rule.header || '') +
          ' -> ' + (rule.category || rule.action || '')
      },

      addClassifier: function (provider) {
        this.classifiers.push({
          _id: String(Date.now()) + String(this.classifiers.length),
          provider: provider,
          model: '',
          base_url: '',
          key_env: provider === 'jev' ? 'TYPESAFE_API_KEY' : '',
          key_set: false,
          min_confidence: 0,
          min_margin: 0,
          rps: 0,
          burst: 0,
          price_input: 0,
          price_output: 0
        })
      },

      removeClassifier: function (item) {
        var next = []
        for (var i = 0; i < this.classifiers.length; i++) {
          if (this.classifiers[i] !== item) {
            next.push(this.classifiers[i])
          }
        }
        this.classifiers = next
      },

      saveClassifiers: async function () {
        var items = []
        for (var i = 0; i < this.classifiers.length; i++) {
          var item = this.classifiers[i]
          items.push({
            provider: item.provider,
            model: item.model,
            base_url: item.base_url,
            key_env: item.key_env,
            min_confidence: Number(item.min_confidence) || 0,
            min_margin: Number(item.min_margin) || 0,
            rps: Number(item.rps) || 0,
            burst: Number(item.burst) || 0,
            price_input: Number(item.price_input) || 0,
            price_output: Number(item.price_output) || 0
          })
        }
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/classifiers', { classifiers: items })
          this.copyBoot()
          this.notice = this.text('classifier.saved')
        })
      },

      finishWizard: async function () {
        if (this.classifiers.length) {
          await this.saveClassifiers()
        }
        this.screen = 'run'
        this.step = 0
      },

      showFields: async function () {
        await this.guard(async function () {
          var data = await this.api('POST', '/api/preview', this.sample)
          this.fields = data.fields || []
          this.result = null
          this.previewNote = this.text('try.nothing')
        })
      },

      classifySample: async function () {
        this.pending = 'try'
        await this.guard(async function () {
          var data = await this.api('POST', '/api/try', this.sample)
          this.fields = data.fields || []
          this.result = data.result
          this.previewNote = ''
        })
      },

      resultLine: function () {
        if (!this.result) {
          return ''
        }
        return this.result.category + ' ' + this.result.confidence + ' ' + this.result.source + ' ' +
          this.result.action + ' ' + this.result.folder + ' ' + (this.result.reason || '')
      },

      openRun: async function () {
        this.go('run')
        if (!this.runForm.profile && this.boot.profiles.length) {
          this.runForm.profile = this.boot.profiles[0].name
        }
        await this.guard(async function () {
          if (!this.runForm.profile) {
            return
          }
          this.report = await this.api('GET', '/api/run?profile=' + encodeURIComponent(this.runForm.profile))
        })
      },

      plan: async function () {
        this.pending = 'plan'
        await this.guard(async function () {
          this.report = await this.api('POST', '/api/plan', {
            profile: this.runForm.profile,
            folder: this.runForm.folder,
            limit: Number(this.runForm.limit) || 200,
            unread: !!this.runForm.unread,
            since: this.runForm.since,
            include_flagged: !!this.runForm.include_flagged,
            include_drafts: !!this.runForm.include_drafts
          })
          this.notice = this.text(this.report.note_key)
        })
      },

      confirmRun: async function () {
        await this.guard(async function () {
          var data = await this.api('POST', '/api/confirm', { profile: this.runForm.profile })
          this.notice = this.text(data.note_key)
          await this.load()
        })
      },

      applyRun: async function () {
        await this.guard(async function () {
          this.report = await this.api('POST', '/api/apply', {
            profile: this.runForm.profile,
            copy_only: !!this.runForm.copy_only
          })
          this.notice = this.text(this.report.note_key)
        })
      },

      undoRun: async function () {
        await this.guard(async function () {
          this.report = await this.api('POST', '/api/undo', { profile: this.runForm.profile })
          this.notice = this.text(this.report.note_key)
        })
      },

      overrideRow: async function () {
        await this.guard(async function () {
          this.report = await this.api('POST', '/api/override', {
            profile: this.runForm.profile,
            uid: Number(this.overrideUID) || 0,
            category: this.overrideCategory
          })
          this.notice = this.text('run.override')
        })
      },

      openActivity: async function () {
        this.go('activity')
        await this.guard(async function () {
          this.activity = await this.api('GET', '/api/activity')
        })
      },

      saveSettings: async function () {
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/settings', {
            language: this.settings.language,
            privacy: {
              redact_email: !!this.settings.privacy.redact_email,
              redact_phone: !!this.settings.privacy.redact_phone,
              subject_only: !!this.settings.privacy.subject_only,
              rules_only: !!this.settings.privacy.rules_only,
              local_only: !!this.settings.privacy.local_only
            }
          })
          this.copyBoot()
          this.notice = this.text('settings.saved')
        })
      },

      acceptConsent: async function () {
        await this.guard(async function () {
          this.boot = await this.api('POST', '/api/consent')
          this.copyBoot()
          this.consentOpen = false
          this.notice = this.text('consent.saved')
          var next = this.afterConsent
          this.afterConsent = ''
          if (next === 'plan') {
            await this.plan()
          }
          if (next === 'try') {
            await this.classifySample()
          }
        })
      },

      copyBoot: function () {
        this.categories = this.boot.categories || []
        this.rules = this.boot.rules || []
        this.classifiers = this.boot.classifiers || []
        for (var i = 0; i < this.classifiers.length; i++) {
          if (!this.classifiers[i]._id) {
            this.classifiers[i]._id = 'c' + String(i)
          }
        }
        this.settings.language = this.boot.language_setting || 'auto'
        this.settings.privacy = this.boot.privacy || blankPrivacy()
      },

      guard: async function (fn) {
        this.error = ''
        try {
          await fn.call(this)
        } catch (err) {
          if (err.consent) {
            this.consentOpen = true
            this.afterConsent = this.pending || 'try'
            this.error = err.message
            return
          }
          this.error = err.message
        }
      },

      activityLine: function (run) {
        var cost = run.has_cost ? (' $' + run.cost_usd) : ''
        return '#' + run.id + ' ' + run.profile + ' ' + run.mailbox + ' ' + run.status +
          ' ' + run.created_at + ' moves=' + run.moves + ' copies=' + run.copies +
          ' errors=' + run.errors + ' api=' + run.api_calls + cost
      }
    }
  })
})

function blankDraft() {
  return {
    provider: '',
    name: '',
    username: '',
    email: '',
    password_env: 'MAIL_SORTER_PASSWORD_',
    host: '',
    host_id: '',
    port: '',
    security: '',
    discover: false,
    max_chars: ''
  }
}

function blankPrivacy() {
  return {
    redact_email: false,
    redact_phone: false,
    subject_only: false,
    rules_only: false,
    local_only: false
  }
}
